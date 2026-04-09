/**
 * Teleport
 * Copyright (C) 2024  Gravitational, Inc.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

import { useCallback, useEffect, useState } from 'react';

import { Box, ButtonPrimary, ButtonSecondary, Flex, Indicator } from 'design';
import { Danger } from 'design/Alert';
import Dialog, {
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from 'design/Dialog';
import {
  FieldSelect,
  FieldSelectCreatable,
} from 'shared/components/FieldSelect';
import { Option } from 'shared/components/Select';
import Validation from 'shared/components/Validation';
import { requiredField } from 'shared/components/Validation/rules';
import { useAsync } from 'shared/hooks/useAsync';
import { getDbNameRequirement } from 'shared/services/databases';

import { useTeleport } from 'teleport';
import { DbConnectData } from 'teleport/lib/term/tty';
import { Database } from 'teleport/services/databases';

export function ConnectDialog(props: {
  clusterId: string;
  serviceName: string;
  onClose(): void;
  onConnect(data: DbConnectData): void;
  desiredDbUser?: string;
  desiredDbName?: string;
  desiredDbRole?: string;
}) {
  // Fetch database information to pre-fill the connection parameters.
  // If desired principals all resolve to valid options, auto-connect
  // directly from the fetch callback (bypassing the form entirely).
  const ctx = useTeleport();
  const [attempt, getDatabase] = useAsync(
    useCallback(async () => {
      const response = await ctx.resourceService.fetchUnifiedResources(
        props.clusterId,
        {
          query: `name == "${props.serviceName}"`,
          kinds: ['db'],
          sort: { fieldName: 'name', dir: 'ASC' },
          limit: 1,
        }
      );

      // TODO(gabrielcorado): Handle scenarios where there is conflict on the name.
      if (response.agents.length !== 1 || response.agents[0].kind !== 'db') {
        throw new Error('Unable to retrieve database information.');
      }

      const db = response.agents[0];
      const autoConnectData = tryAutoConnect(db, props);
      if (autoConnectData) {
        props.onConnect(autoConnectData);
        return null;
      }

      return db;
    }, [props.clusterId, ctx.resourceService, props.serviceName])
  );

  useEffect(() => {
    void getDatabase();
  }, [getDatabase]);

  return (
    <Dialog
      dialogCss={dialogCss}
      disableEscapeKeyDown={false}
      onClose={props.onClose}
      open={true}
    >
      <DialogHeader mb={4}>
        <DialogTitle>Connect To Database</DialogTitle>
      </DialogHeader>

      {attempt.status === 'error' && <Danger children={attempt.statusText} />}
      {(attempt.status === '' || attempt.status === 'processing') && (
        <Box textAlign="center" m={10}>
          <Indicator />
        </Box>
      )}
      {attempt.status === 'success' && attempt.data && (
        <ConnectForm
          db={attempt.data}
          onConnect={props.onConnect}
          onClose={props.onClose}
          desiredDbUser={props.desiredDbUser}
          desiredDbName={props.desiredDbName}
          desiredDbRole={props.desiredDbRole}
        />
      )}
    </Dialog>
  );
}

function ConnectForm(props: {
  db: Database;
  onConnect(data: DbConnectData): void;
  onClose(): void;
  desiredDbUser?: string;
  desiredDbName?: string;
  desiredDbRole?: string;
}) {
  const { options: dbNamesOpts, hasWildcard: dbNameHasWildcard } =
    prepareOptions(props.db.names);
  const { options: dbUserOpts, hasWildcard: dbUserHasWildcard } =
    prepareOptions(props.db.users);
  const { options: dbRolesOpts, hasWildcard: dbRoleHasWildcard } =
    prepareOptions(props.db.roles);

  const [selectedName, setSelectedName] = useState<Option>(() =>
    findOptionOrCreate(dbNamesOpts, props.desiredDbName, dbNameHasWildcard) ??
    dbNamesOpts?.[0]
  );
  const [selectedUser, setSelectedUser] = useState<Option>(() =>
    findOptionOrCreate(dbUserOpts, props.desiredDbUser, dbUserHasWildcard) ??
    dbUserOpts?.[0]
  );
  const [selectedRoles, setSelectedRoles] =
    useState<readonly Option[]>(() => {
      if (props.desiredDbRole) {
        const match = findOptionOrCreate(dbRolesOpts, props.desiredDbRole, dbRoleHasWildcard);
        if (match) return [match];
      }
      return dbRolesOpts;
    });

  const dbConnect = () => {
    props.onConnect({
      serviceName: props.db.name,
      protocol: props.db.protocol,
      dbName: selectedName?.value,
      dbUser: selectedUser.value,
      dbRoles: selectedRoles?.map(role => role.value),
    });
  };

  const dbNameReq = getDbNameRequirement(props.db.protocol);
  return (
    <Validation>
      {({ validator }) => (
        <form>
          <DialogContent flex="0 0 auto">
            {dbNameReq !== 'unsupported' && (
              <ConnectionField
                // if db name is optional, then RBAC is not enforcing a db name, so they can input whatever they want
                allowCreatableSelect={
                  dbNameHasWildcard || dbNameReq === 'optional'
                }
                label="Database name"
                menuPosition="fixed"
                onChange={option => setSelectedName(option as Option)}
                value={selectedName}
                options={dbNamesOpts}
                creatableOptions={{
                  formatCreateLabel: (userInput: string) =>
                    `Use "${userInput}" database name`,
                  toolTipContent:
                    'You can type in the select box to use a custom database name instead of the available options.',
                }}
                isClearable={dbNameReq === 'optional'}
                rule={
                  dbNameReq === 'required'
                    ? requiredField('Database name is required')
                    : undefined
                }
              />
            )}
            <ConnectionField
              allowCreatableSelect={dbUserHasWildcard}
              label="Database user"
              menuPosition="fixed"
              onChange={option => setSelectedUser(option as Option)}
              value={selectedUser}
              options={dbUserOpts}
              creatableOptions={{
                formatCreateLabel: userInput =>
                  `Use "${userInput}" database user`,
                toolTipContent:
                  'You can type in the select box to use a custom database user instead of the available options.',
              }}
              rule={requiredField('Database user is required')}
              isDisabled={props.db.autoUsersEnabled}
              helperText={
                props.db.autoUsersEnabled
                  ? 'Using an auto provisioned user, you cannot change the database user.'
                  : null
              }
            />
            {(dbRolesOpts?.length > 0 || dbRoleHasWildcard) && (
              <ConnectionField
                allowCreatableSelect={dbRoleHasWildcard}
                label="Database roles"
                menuPosition="fixed"
                isMulti={true}
                onChange={setSelectedRoles}
                value={selectedRoles}
                options={dbRolesOpts}
                creatableOptions={{
                  formatCreateLabel: userInput =>
                    `Use "${userInput}" database role`,
                  toolTipContent:
                    'You can type in the select box to use a custom database role in addition to the available options.',
                }}
                rule={requiredField('At least one database role is required')}
              />
            )}
          </DialogContent>
          <DialogFooter>
            <Flex alignItems="center" justifyContent="space-between">
              <ButtonSecondary
                type="button"
                width="45%"
                size="large"
                onClick={props.onClose}
              >
                Close
              </ButtonSecondary>
              <ButtonPrimary
                type="submit"
                width="45%"
                size="large"
                onClick={e => {
                  e.preventDefault();
                  validator.validate() && dbConnect();
                }}
              >
                Connect
              </ButtonPrimary>
            </Flex>
          </DialogFooter>
        </form>
      )}
    </Validation>
  );
}

function ConnectionField({
  allowCreatableSelect,
  creatableOptions = {},
  ...commonOptions
}) {
  return allowCreatableSelect ? (
    <FieldSelectCreatable {...commonOptions} {...creatableOptions} />
  ) : (
    <FieldSelect {...commonOptions} />
  );
}

/**
 * Checks if desired principals all resolve to valid options for this DB.
 * If so, returns the DbConnectData to use; otherwise returns null.
 */
function tryAutoConnect(
  db: Database,
  props: { desiredDbUser?: string; desiredDbName?: string; desiredDbRole?: string }
): DbConnectData | null {
  if (!props.desiredDbUser && !props.desiredDbRole) return null;

  const { options: nameOpts, hasWildcard: nameWild } = prepareOptions(db.names);
  const { options: userOpts, hasWildcard: userWild } = prepareOptions(db.users);
  const { options: roleOpts, hasWildcard: roleWild } = prepareOptions(db.roles);

  const dbNameReq = getDbNameRequirement(db.protocol);
  const resolvedName = findOptionOrCreate(nameOpts, props.desiredDbName, nameWild);
  const resolvedUser = findOptionOrCreate(userOpts, props.desiredDbUser, userWild);
  const resolvedRole = findOptionOrCreate(roleOpts, props.desiredDbRole, roleWild);

  const hasName = dbNameReq === 'unsupported' || !!resolvedName;
  const hasUserOrRole = !!resolvedUser || !!resolvedRole;

  if (!hasName || !hasUserOrRole) return null;

  return {
    serviceName: db.name,
    protocol: db.protocol,
    dbName: resolvedName?.value,
    dbUser: resolvedUser?.value ?? userOpts?.[0]?.value,
    dbRoles: resolvedRole ? [resolvedRole.value] : roleOpts?.map(o => o.value),
  };
}

function findOptionOrCreate(
  options: Option[],
  desired: string | undefined,
  hasWildcard: boolean
): Option | undefined {
  if (!desired) return undefined;
  const match = options?.find(o => o.value === desired);
  if (match) return match;
  // If there's a wildcard, the user can use any value, so create an option.
  if (hasWildcard) return { value: desired, label: desired };
  return undefined;
}

function prepareOptions(rawOpts: string[]): {
  options: Option[];
  hasWildcard: boolean;
} {
  let hasWildcard = false;
  const options = rawOpts
    ?.map(role => ({
      value: role,
      label: role,
    }))
    .filter(({ value }: Option) => {
      if (value === '*') {
        hasWildcard = true;
        return false;
      }

      return true;
    });

  return { options, hasWildcard };
}

const dialogCss = () => `
  min-height: 200px;
  max-width: 600px;
  width: 100%;
`;
